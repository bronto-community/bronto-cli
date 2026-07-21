# syntax=docker/dockerfile:1
# Release image: expects PREBUILT per-arch binaries staged by release.yml
# under docker-ctx/linux/<arch>/bronto (goreleaser already cross-compiled
# them natively). No Go compilation happens here — compiling inside this
# Dockerfile under QEMU cost ~8-10 minutes per release.
# For a from-source local image: make snapshot, then stage dist/ the same
# way (see the "Stage docker build context" step in release.yml).

# CA certs are architecture-independent: run this stage on the BUILD
# platform so it never executes under emulation.
FROM --platform=$BUILDPLATFORM alpine:3 AS certs
RUN apk --no-cache add ca-certificates

FROM scratch
ARG TARGETPLATFORM
ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.source="https://github.com/bronto-community/bronto-cli" \
	org.opencontainers.image.description="Community CLI for the Bronto observability platform" \
	org.opencontainers.image.licenses="MIT" \
	org.opencontainers.image.version="${VERSION}" \
	org.opencontainers.image.revision="${COMMIT}"
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY ${TARGETPLATFORM}/bronto /bronto
# Distroless' conventional nonroot uid/gid; nothing in the image needs root.
USER 65532:65532
ENTRYPOINT ["/bronto"]
