# Build management.html
FROM oven/bun:1.3.14 AS react-builder
WORKDIR /app
ARG CPAM_VERSION=v0.0.0
ARG CPAM_COMMIT=unknown
COPY frontend/package.json frontend/bun.lock ./
RUN bun install --frozen-lockfile
COPY frontend/. .
RUN VERSION="${CPAM_VERSION}" bun run build && \ 
 cp dist/index.html /app/management.html

# Build CPA
FROM --platform=$BUILDPLATFORM golang:trixie AS go-builder
ENV TZ="Asia/Jakarta"
RUN [ ! -f /etc/localtime ] && ln -s /usr/share/zoneinfo/$TZ /etc/localtime; 	\
    echo $TZ > /etc/timezone
WORKDIR /app
# Define the build arguments passed from GitHub Actions
ARG CPA_VERSION=v0.0.0
ARG CPA_COMMIT=unknown
RUN set -eux;     \
    apt update -y; \
    apt install -y --no-install-recommends       \
        ca-certificates       \
        build-essential       \
        git;     \
    apt-mark showmanual > /savedAptMark.txt
RUN set -eux;   \
    apt-mark auto '.*' > /dev/null ;	\
    apt-mark manual $(cat /savedAptMark.txt) > /dev/null; 	\
    apt purge -y --auto-remove -o APT::AutoRemove::RecommendsImportant=false;     \
    apt clean;     \
    apt autoclean;     \
    rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN set -eux;   \
    export BUILD_DATE="$(date +%Y-%m-%d)";   \
    CGO_ENABLED=1 \
        GOOS=linux \
        go build \
            -buildvcs=false \
            -ldflags="-s -w -X 'main.Version=${CPA_VERSION}' -X 'main.Commit=${CPA_COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
            -o ./CLIProxyAPI ./cmd/server/ ;  \
    chmod +x ./CLIProxyAPI

FROM debian:trixie-slim
ARG HOME_DIR=/root
ARG CLIPROXY_INSTALLDIR=${HOME_DIR}/.cliproxyapi/bin
ENV TZ="Asia/Jakarta"
ENV PATH=$HOME_DIR/.cliproxyapi/bin:$PATH
SHELL ["/bin/bash", "-c"]
WORKDIR ${HOME_DIR}
EXPOSE 8317
RUN set -eux; 	\
    [ ! -f /etc/localtime ] && ln -s /usr/share/zoneinfo/$TZ /etc/localtime; 	\
    echo $TZ > /etc/timezone; 	\
    apt-get update
RUN set -eux;     \
    apt install -y --no-install-recommends \
        tzdata ca-certificates;     \
    apt-mark showmanual > /savedAptMark.txt

# install cliproxy
COPY --from=go-builder /app/CLIProxyAPI  ${CLIPROXY_INSTALLDIR}/cli-proxy-api
COPY --from=react-builder /app/management.html ${CLIPROXY_INSTALLDIR}/static/management.html
RUN set -eux;   \
    apt-mark auto '.*' > /dev/null ;	\
    apt-mark manual $(cat /savedAptMark.txt) > /dev/null; 	\
    apt-get purge -y --auto-remove -o APT::AutoRemove::RecommendsImportant=false;     \
    apt-get clean;     \
    apt-get autoclean;     \
    rm -rf /var/lib/apt/lists/*

RUN set -eux;     \
    touch ${HOME_DIR}/cpa.sh;    \
    chmod +x ${HOME_DIR}/cpa.sh;     \
    cat <<EOF > ${HOME_DIR}/cpa.sh
#!/bin/bash
cd "${CLIPROXY_INSTALLDIR}"
echo "---------------------------------------------------"
echo "To open CLIProxyAPI, please visit:"
echo "http://localhost:8317/management.html or"
echo ""
echo "---------------------------------------------------"
./cli-proxy-api
EOF
CMD ["./cpa.sh"]
