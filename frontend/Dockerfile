FROM node:20-alpine

ARG LOCAL_UID=1000
ARG LOCAL_GID=1000

RUN mkdir -p /app \
    && chown "${LOCAL_UID}:${LOCAL_GID}" /app

WORKDIR /app

COPY --chown=${LOCAL_UID}:${LOCAL_GID} package*.json ./

USER ${LOCAL_UID}:${LOCAL_GID}

RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi

COPY --chown=${LOCAL_UID}:${LOCAL_GID} . ./

EXPOSE 5173
CMD ["npm", "run", "dev"]
