FROM golang:1.25.0-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY cmd/browser ./cmd/browser
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o /browser ./cmd/browser

FROM mcr.microsoft.com/playwright:v1.59.1-noble@sha256:040190be07ce081a025d95f2aeab57b588bed4f19165c1c93cb765372d368463 AS browsers
ENV CLOAKBROWSER_CACHE_DIR=/opt/cloakbrowser CLOAKBROWSER_AUTO_UPDATE=false
WORKDIR /tmp/browser-install
RUN npm install --ignore-scripts --save-exact cloakbrowser@0.3.25 playwright-core@1.59.1 && node --input-type=module -e 'import {ensureBinary} from "cloakbrowser"; import {writeFileSync} from "node:fs"; writeFileSync("/opt/cloak-path",await ensureBinary())'
# Pin the actual ARM64 executable as well as the npm wrapper/version.
RUN printf '%s  %s\n' '3cf5231598c4fd44be1ed10c58cab4a1d59cd18b05a4800c73b946e0bfc4d34f' "$(cat /opt/cloak-path)" | sha256sum -c -
RUN mkdir /opt/browsers && ln -s "$(cat /opt/cloak-path)" /opt/browsers/cloak && ln -s "$(find /ms-playwright -name chrome -type f | head -1)" /opt/browsers/chromium

FROM browsers AS release
COPY --from=build /browser /usr/local/bin/bank-browser
COPY third_party/cloakbrowser-0.3.25/LICENSE /usr/share/licenses/cloakbrowser/LICENSE
COPY third_party/cloakbrowser-0.3.25/PLAYWRIGHT-LICENSE /usr/share/licenses/cloakbrowser/PLAYWRIGHT-LICENSE
RUN rm -rf /tmp/browser-install /usr/local/lib/node_modules /usr/local/bin/node /usr/local/bin/npm /usr/local/bin/npx /usr/bin/node /usr/bin/npm /usr/bin/npx
ENV CHROMIUM_PATH=/opt/browsers/chromium CLOAK_PATH=/opt/browsers/cloak TZ=America/Halifax
USER pwuser
ENTRYPOINT ["timeout","--signal=TERM","--kill-after=15s","300s","xvfb-run","-a","/usr/local/bin/bank-browser"]
