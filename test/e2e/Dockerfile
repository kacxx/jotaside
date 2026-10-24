# Local clean-room: runs the e2e flow (test/e2e/run.sh) as a brand-new,
# unprivileged user with an empty HOME and no prior jotaside state, installing the
# published @latest exactly as a first-time user would.
#
#   docker build -t jotaside-e2e -f test/e2e/Dockerfile .
#   docker run --rm jotaside-e2e
#
# CI runs run.sh directly on the runner (no Docker); this image is for a clean
# local reproduction.
#
# Behind a TLS-intercepting corporate proxy, in-container HTTPS (the Go module
# proxy) will fail to verify certificates. Trust your proxy CA at build time,
# e.g. with an override that appends it before `go install`:
#   RUN cat your-proxy-ca.pem >> /etc/ssl/certs/ca-certificates.crt
# Keep that out of this file and out of git; it is environment-specific.
FROM golang:1.26-bookworm

RUN useradd -m tester
USER tester
WORKDIR /home/tester

# Reproduce the real first-time-user case: GOPATH under HOME, with ~/go/bin NOT
# on PATH (the base image only puts the original /go/bin there). This makes the
# harness assert the "'jotaside' is not on PATH" situation the README warns about,
# so the "use the absolute binary path in configs" guidance is tested honestly.
ENV GOPATH=/home/tester/go
ENV JOTASIDE_E2E_EXPECT_NOT_ON_PATH=1

COPY --chown=tester:tester run.sh /home/tester/run.sh
ENTRYPOINT ["bash", "/home/tester/run.sh"]
