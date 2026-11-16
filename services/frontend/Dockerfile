# Build stage
FROM node:20-alpine AS builder

WORKDIR /app

# Copy package files
COPY package*.json ./

# Install dependencies
# Using npm install instead of npm ci since package-lock.json may not exist
RUN npm install

# Copy source code
COPY . .

# Set build-time environment variables
ARG VITE_API_URL=/api
ARG VITE_PUBLIC_POSTHOG_KEY
ARG VITE_PUBLIC_POSTHOG_HOST
ARG VITE_DISABLE_PASSWORD_AUTH

ENV VITE_API_URL=$VITE_API_URL
ENV VITE_PUBLIC_POSTHOG_KEY=$VITE_PUBLIC_POSTHOG_KEY
ENV VITE_PUBLIC_POSTHOG_HOST=$VITE_PUBLIC_POSTHOG_HOST
ENV VITE_DISABLE_PASSWORD_AUTH=$VITE_DISABLE_PASSWORD_AUTH

# Build the application
RUN npm run build

# Production stage
FROM nginx:alpine

# Copy built files from builder
COPY --from=builder /app/dist /usr/share/nginx/html

# Copy templated nginx config (envsubst substitutes BACKEND_UPSTREAM & NGINX_RESOLVER)
# 15-default-resolver.sh auto-detects Docker/GKE DNS resolver when NGINX_RESOLVER is unset.
ENV BACKEND_UPSTREAM=http://mia-backend:8080
ENV NGINX_ENVSUBST_FILTER=^(BACKEND_UPSTREAM|NGINX_RESOLVER)$
COPY 15-default-resolver.sh /docker-entrypoint.d/15-default-resolver.sh
RUN chmod +x /docker-entrypoint.d/15-default-resolver.sh
COPY nginx.conf.template /etc/nginx/templates/default.conf.template

# Expose port
EXPOSE 80

# Start nginx
CMD ["nginx", "-g", "daemon off;"]

