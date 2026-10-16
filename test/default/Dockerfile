FROM public.ecr.aws/aws-cli/aws-cli:2.36.21@sha256:fb09d91d0640088f15475d9a9366a215c88a24f9871ae0f6124bede0d2157e9a AS awscli
FROM golang:1.25.4-bookworm@sha256:e17419604b6d1f9bc245694425f0ec9b1b53685c80850900a376fb10cb0f70cb

ARG TARGETARCH
COPY --from=awscli /usr/local/aws-cli /usr/local/aws-cli
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) minio_sha256='7c5bd8512c6e966455b1d198209358b2d191c77a83ab377c4073281065fb855f' ;; \
      arm64) minio_sha256='5c83cd2cf151717ba0243f73e1c7802ff36e272b67144bdd7f1f7d684fd6f03d' ;; \
      *) printf 'unsupported MinIO architecture: %s\n' "${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl --fail --location --retry 3 --retry-all-errors \
      --output /tmp/minio \
      "https://github.com/minio/minio/releases/download/RELEASE.2025-09-07T16-13-09Z/minio.linux-${TARGETARCH}.RELEASE.2025-09-07T16-13-09Z"; \
    printf '%s  %s\n' "${minio_sha256}" /tmp/minio | sha256sum --check; \
    install -m 0755 /tmp/minio /usr/bin/minio; \
    rm /tmp/minio

RUN ln -s /usr/local/aws-cli/v2/current/bin/aws /usr/local/bin/aws
COPY scripts/private/install-root-test-dependencies.sh /usr/local/bin/install-root-test-dependencies
RUN /usr/local/bin/install-root-test-dependencies \
 && rm -f /usr/local/bin/install-root-test-dependencies

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download \
 && go mod verify

COPY . /workspace
ARG TRANSFERLANES_DEVELOPMENT_COMMIT
ARG TRANSFERLANES_DEVELOPMENT_MODIFIED
RUN test ! -e /workspace/.git \
 && test -n "${TRANSFERLANES_DEVELOPMENT_COMMIT}" \
 && { test "${TRANSFERLANES_DEVELOPMENT_MODIFIED}" = true || test "${TRANSFERLANES_DEVELOPMENT_MODIFIED}" = false; }
ENV TRANSFERLANES_DEVELOPMENT_COMMIT=${TRANSFERLANES_DEVELOPMENT_COMMIT}
ENV TRANSFERLANES_DEVELOPMENT_MODIFIED=${TRANSFERLANES_DEVELOPMENT_MODIFIED}

ENTRYPOINT ["/workspace/test/default/run.sh"]
