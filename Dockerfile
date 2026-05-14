FROM node:22-bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends tar git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /work
ENV CI=1
ENV NODE_ENV=test
CMD ["bash"]
