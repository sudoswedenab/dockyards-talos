FROM docker.io/library/golang:1.25.1 AS builder
COPY . /src
WORKDIR /src
ENV CGO_ENABLED=0
RUN go build -o dockyards-talos -ldflags="-s -w"

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /src/dockyards-talos /usr/bin/dockyards-talos
ENTRYPOINT ["/usr/bin/dockyards-talos"]
