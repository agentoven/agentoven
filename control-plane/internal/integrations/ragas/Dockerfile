FROM python:3.11-slim

WORKDIR /app

# Install RAGAS and dependencies
RUN pip install --no-cache-dir \
    ragas>=0.1.0 \
    fastapi>=0.100.0 \
    uvicorn[standard]>=0.23.0 \
    datasets>=2.14.0

COPY server.py .

EXPOSE 8400

ENV RAGAS_PORT=8400

# Health check
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://localhost:8400/health')"

CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8400"]
