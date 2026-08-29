FROM --platform=$BUILDPLATFORM golang:1.25 AS builder

WORKDIR /src

ENV CGO_ENABLED=0 GOWORK=off

ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" \
    -o /out/ainovel-cli \
    ./cmd/ainovel-cli && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" \
    -o /out/ainovel-web-host \
    ./cmd/ainovel-web-host

FROM alpine:3.22

RUN apk add --no-cache \
    ca-certificates \
    tzdata

WORKDIR /workspace

COPY --from=builder /out/ainovel-cli /usr/local/bin/ainovel-cli
COPY --from=builder /out/ainovel-web-host /usr/local/bin/ainovel-web-host

EXPOSE 8080

ENTRYPOINT ["ainovel-web-host"]
CMD ["--config", "/root/.ainovel/config.json", "--addr", "0.0.0.0:8080"]
