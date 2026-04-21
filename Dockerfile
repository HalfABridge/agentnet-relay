FROM golang:1.24-bookworm AS installer

RUN GOBIN=/out go install github.com/betta-lab/agentnet-relay/cmd/relay@latest

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata wget \
    && rm -rf /var/lib/apt/lists/*

RUN groupadd --system app && useradd --system --gid app --home /app app
RUN mkdir -p /data && chown -R app:app /data

WORKDIR /app
COPY --from=installer /out/relay /usr/local/bin/agentnet-relay
RUN chmod +x /usr/local/bin/agentnet-relay

USER app

EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["agentnet-relay"]
CMD ["-addr", ":8080", "-db", "/data/agentnet.db"]
