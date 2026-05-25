FROM docker.io/library/golang:1.25.3 AS builder
COPY . /src
WORKDIR /src
ENV CGO_ENABLED=0
RUN go build -ldflags="-s -w" ./cmd/dockyards-talos
RUN go build -ldflags="-s -w" ./cmd/discovery-service

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /src/dockyards-talos /usr/bin/dockyards-talos
COPY --from=builder /src/discovery-service /usr/bin/discovery-service
ENTRYPOINT ["/usr/bin/dockyards-talos"]
