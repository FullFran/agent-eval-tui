# Two stages, and the runtime has no distribution in it at all.
#
# CGO_ENABLED=0 is what makes that possible: modernc.org/sqlite is pure Go, so
# the binary has no libc to link against and `scratch` is a valid base. The
# resulting image is the binary plus CA certificates, which is also the whole
# attack surface.
FROM golang:1.26-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/agent-eval ./cmd/agent-eval

# The data directory has to be created here, owned by the runtime uid.
# `scratch` has no shell, so there is no mkdir or chown at runtime, and Docker
# seeds a named volume from the image's directory — including its ownership. Get
# this wrong and the container starts, then dies with
# "apply schema: unable to open database file", which reads like a SQLite
# problem rather than a permissions one.
RUN mkdir -p /data && chown 65532:65532 /data

FROM scratch AS runtime

# Only needed if a future exporter or scorer makes outbound TLS calls. Ingest
# itself does not, but shipping without them turns that future change into a
# baffling x509 error.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=build /out/agent-eval /agent-eval
COPY --from=build --chown=65532:65532 /data /data

# Non-root by uid: `scratch` has no /etc/passwd for a named user to exist in.
USER 65532:65532

# The database lives here so a volume mount has an obvious target.
VOLUME ["/data"]

EXPOSE 4318

ENTRYPOINT ["/agent-eval"]
CMD ["serve", "-addr", ":4318", "-db", "/data/agent-eval.db"]
