FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY cmd/browser ./cmd/browser
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -p=2 -trimpath -ldflags='-s -w' -o /browser ./cmd/browser

FROM mcr.microsoft.com/playwright:v1.59.1-noble@sha256:040190be07ce081a025d95f2aeab57b588bed4f19165c1c93cb765372d368463 AS browsers
ENV CLOAKBROWSER_CACHE_DIR=/opt/cloakbrowser CLOAKBROWSER_AUTO_UPDATE=false
WORKDIR /tmp/browser-install
RUN npm install --ignore-scripts --save-exact cloakbrowser@0.3.25 playwright-core@1.59.1 && node --input-type=module -e 'import {ensureBinary} from "cloakbrowser"; import {writeFileSync} from "node:fs"; writeFileSync("/opt/cloak-path",await ensureBinary())'
# Pin the actual ARM64 executable as well as the npm wrapper/version.
RUN printf '%s  %s\n' '3cf5231598c4fd44be1ed10c58cab4a1d59cd18b05a4800c73b946e0bfc4d34f' "$(cat /opt/cloak-path)" | sha256sum -c -
RUN mkdir /opt/browsers && ln -s "$(cat /opt/cloak-path)" /opt/browsers/cloak && ln -s "$(find /ms-playwright -name chrome -type f | head -1)" /opt/browsers/chromium
# The key is available only to this build step; it never enters image layers.
# The official downloader verifies the signed manifest and archive checksum.
RUN --mount=type=secret,id=cloak_license,required=true npm install --ignore-scripts --save-exact --prefix /tmp/nbdb-browser-install cloakbrowser@0.6.0 playwright-core@1.59.1 && CLOAKBROWSER_LICENSE_KEY="$(cat /run/secrets/cloak_license)" CLOAKBROWSER_CACHE_DIR=/opt/cloakbrowser-nbdb node --input-type=module -e 'import {ensureBinary} from "/tmp/nbdb-browser-install/node_modules/cloakbrowser/dist/index.js"; import {writeFileSync} from "node:fs"; const p=await ensureBinary(); if(!p.includes("154.0.8037.57.1")) throw new Error("Unexpected NBDB browser version"); writeFileSync("/opt/nbdb-cloak-path",p)'
RUN printf '%s  %s\n' '8eba2a336aac8e48dd573c2cdc34e6619b223f2f32aeecea4ac70bc97c9321d0' "$(cat /opt/nbdb-cloak-path)" | sha256sum -c - && ln -s "$(cat /opt/nbdb-cloak-path)" /opt/browsers/nbdb-cloak && rm -f /opt/cloakbrowser-nbdb/.license_cache

# A full runtime: the browsers above plus licences, without Node. Build this
# target only to refresh browser binaries deliberately, verify it with real
# sign-ins, push it, and pin its digest in the release stage below.
FROM browsers AS runtime
COPY third_party/cloakbrowser-0.3.25/LICENSE /usr/share/licenses/cloakbrowser/LICENSE
COPY third_party/cloakbrowser-0.3.25/PLAYWRIGHT-LICENSE /usr/share/licenses/cloakbrowser/PLAYWRIGHT-LICENSE
COPY third_party/cloakbrowser-0.6.0/LICENSE /usr/share/licenses/cloakbrowser/LICENSE-0.6.0
RUN rm -rf /tmp/browser-install /tmp/nbdb-browser-install /usr/local/lib/node_modules /usr/local/bin/node /usr/local/bin/npm /usr/local/bin/npx /usr/bin/node /usr/bin/npm /usr/bin/npx
ENV CHROMIUM_PATH=/opt/browsers/chromium CLOAK_PATH=/opt/browsers/cloak NBDB_CLOAK_PATH=/opt/browsers/nbdb-cloak NBDB_BROWSER_VERSION=154.0.8037.57.1 TZ=America/Halifax
USER pwuser
ENTRYPOINT ["timeout","--signal=TERM","--kill-after=15s","300s","xvfb-run","-a","/usr/local/bin/bank-browser"]

# Releases add only the worker binary to the verified, deployed runtime, pinned
# by digest. The NBDB and Rogers browser builds never change by accident, and
# the licensed browser download does not run on release.
FROM 187489282488.dkr.ecr.ca-central-1.amazonaws.com/cdk-hnb659fds-container-assets-187489282488-ca-central-1@sha256:9b5af1b89670a128757a7b56c1bb39a3d995beab53ba715570a7ff354ffd7ee3 AS release
COPY --from=build /browser /usr/local/bin/bank-browser
