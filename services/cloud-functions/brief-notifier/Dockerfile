FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY main.py .

ENV PORT=8080
EXPOSE 8080

CMD ["sh", "-c", "exec functions-framework --target=brief_notifier --host=0.0.0.0 --port=${PORT:-8080}"]
