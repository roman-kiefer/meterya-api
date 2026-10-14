FROM --platform=$BUILDPLATFORM golang:1.26.5 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /meterya ./cmd/meterya

FROM build AS test
RUN apt-get update && apt-get install -y --no-install-recommends tesseract-ocr tesseract-ocr-eng && rm -rf /var/lib/apt/lists/*
CMD ["go", "test", "-count=1", "-v", "./..."]

FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates tzdata tesseract-ocr tesseract-ocr-data-eng && adduser -D -u 10001 meterya
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"
COPY --from=build /meterya /usr/local/bin/meterya
USER meterya
ENTRYPOINT ["meterya"]
