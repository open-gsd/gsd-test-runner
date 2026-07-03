# NODE_VERSION selects the Node major (Slice 1 of enhancement #108).
# Default stays 22 so a plain `docker build` is unaffected.
ARG NODE_VERSION=22
FROM node:${NODE_VERSION}-bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends tar git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /work
ENV CI=1
ENV NODE_ENV=test
CMD ["bash"]
