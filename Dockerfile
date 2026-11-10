# Build stage: static binary without cgo.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.Version=${VERSION}" \
      -o /out/cslvpn ./cmd/cslvpn

FROM alpine:3.20
RUN apk add --no-cache iproute2 iptables ca-certificates
COPY --from=build /out/cslvpn /usr/local/bin/cslvpn
EXPOSE 7777/udp 7777/tcp
ENTRYPOINT ["/usr/local/bin/cslvpn"]
CMD ["server", "-listen", ":7777"]
