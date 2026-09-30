FROM mcr.microsoft.com/playwright:v1.63.0-noble
WORKDIR /app
COPY --chmod=644 package.json ./
RUN npm install --omit=dev --no-audit --no-fund
COPY --chmod=644 *.mjs ./
USER pwuser
EXPOSE 7778
CMD ["node", "server.mjs"]
