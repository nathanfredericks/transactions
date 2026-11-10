FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY cmd/browser ./cmd/browser
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -p=2 -trimpath -ldflags='-s -w' -o /browser ./cmd/browser
# The driver is playwright-core plus the Node.js release playwright-go expects.
# Node is fetched here, pinned by checksum, because the installer's own fetch
# has no timeout and nodejs.org's .tar.gz stalls; the installer then adds
# playwright-core. No browsers are downloaded.
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends xz-utils >/dev/null && \
    mkdir -p /opt/playwright-driver && \
    curl -fsSL --retry 3 --max-time 300 -o /tmp/node.tar.xz https://nodejs.org/dist/v24.21.0/node-v24.21.0-linux-arm64.tar.xz && \
    echo '6ad1325edbdb5649c379b75a237147a666c95d4f9ae8d340fef2d1575d289ad2  /tmp/node.tar.xz' | sha256sum -c - && \
    tar -xJf /tmp/node.tar.xz -O node-v24.21.0-linux-arm64/bin/node > /opt/playwright-driver/node && \
    chmod 755 /opt/playwright-driver/node && rm /tmp/node.tar.xz && \
    PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver PLAYWRIGHT_NODEJS_PATH=/opt/playwright-driver/node \
    go run github.com/mxschmitt/playwright-go/cmd/playwright --version

# Releases build on the verified browser runtime, pinned by digest: stock
# Chromium for EQ, CloakBrowser 154.0.8037.57.1 for Rogers and NBDB, and the
# fonts and Xvfb they run with. Changing a browser build is a deliberate step:
# build a new runtime, verify real sign-ins with it, push it and pin it here.
FROM 187489282488.dkr.ecr.ca-central-1.amazonaws.com/cdk-hnb659fds-container-assets-187489282488-ca-central-1@sha256:9b5af1b89670a128757a7b56c1bb39a3d995beab53ba715570a7ff354ffd7ee3 AS release
COPY --from=build /opt/playwright-driver /opt/playwright-driver
COPY --from=build /browser /usr/local/bin/bank-browser
ENV PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver \
    CLOAK_PATH=/opt/browsers/nbdb-cloak \
    CLOAK_VERSION=154.0.8037.57.1
