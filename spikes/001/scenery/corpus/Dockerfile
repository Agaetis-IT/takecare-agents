# One image for the five corpus sources. The corpus is NOT baked in: it is
# mounted from a ConfigMap on /corpus, so changing it needs no rebuild.
#
# Debian slim, not alpine: manylinux wheels for pydantic-core and uvicorn are
# always available, while musl may fall back to a local compile.
ARG PYTHON_IMAGE
FROM ${PYTHON_IMAGE}

ARG FASTMCP_VERSION
RUN pip install --no-cache-dir "fastmcp==${FASTMCP_VERSION}"

COPY server.py /app/server.py
WORKDIR /app

# Defaults, overridden by each instance's environment.
ENV TOOL_PREFIX=corpus \
    DOMAIN=Records \
    CORPUS_PATH=/corpus/data.json \
    PORT=3000

USER 65534:65534
EXPOSE 3000
ENTRYPOINT ["python", "-u", "/app/server.py"]
