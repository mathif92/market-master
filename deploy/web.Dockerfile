# Build the SPA, then serve it with nginx which proxies /v1 to the gateway.
FROM node:22-alpine AS build
WORKDIR /app
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM nginx:1.27-alpine
COPY --from=build /app/dist /usr/share/nginx/html
COPY deploy/web-nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
