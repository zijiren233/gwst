FROM golang:1.23-alpine AS builder

COPY ./ ./

ENV CGO_ENABLED=0

RUN go build -trimpath -ldflags="-s -w" -o /gwst .

FROM alpine:latest

ENV PUID=0 PGID=0 UMASK=022

COPY --from=builder /gwst /gwst

RUN apk add --no-cache bash ca-certificates su-exec tzdata && \
    rm -rf /var/cache/apk/*

EXPOSE 8080/tcp

ENTRYPOINT [ "/gwst" ]

CMD [ "/app/config.yaml" ]
