FROM --platform=$BUILDPLATFORM golang:1.25-alpine3.21 AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app
COPY . .
RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /app/fsb -ldflags="-w -s" ./cmd/fsb

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /app/fsb /app/fsb
EXPOSE 8080
ENTRYPOINT ["/app/fsb", "run"]
