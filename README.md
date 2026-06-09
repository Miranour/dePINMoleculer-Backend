# dePIN Backend

The core backend service for the dePIN platform. Built with Go, it provides a robust RESTful API via Gin, high-performance RPC via gRPC, and asynchronous task processing using RabbitMQ and Redis.

## 🚀 Technologies

- **Language:** Go (1.25+)
- **REST API:** Gin (`github.com/gin-gonic/gin`)
- **RPC:** gRPC & Protocol Buffers
- **Database:** PostgreSQL (with `pgx` and `golang-migrate`)
- **Cache & Key-Value Store:** Redis (`redis/go-redis`)
- **Message Broker:** RabbitMQ (`amqp091-go`)
- **Authentication:** JWT (`golang-jwt`)

## 📂 Project Structure

```text
backend/
├── cmd/                # Main applications for this project
│   └── server/         # The primary backend server entrypoint
├── internal/           # Private application and library code
├── pkg/                # Library code that's okay to use by external applications
├── proto/              # Protocol Buffers definitions (.proto files)
├── migrations/         # SQL migration files
├── tasks/              # Asynchronous background tasks/workers
├── validator-engine/   # Separate gRPC service for validation logic
├── Dockerfile          # Dockerfile for the main backend
├── Dockerfile.validator# Dockerfile for the validator engine
└── docker-compose.yml  # Docker Compose for local development
```

## 🛠️ Prerequisites

- [Go 1.25+](https://go.dev/dl/)
- [Docker & Docker Compose](https://docs.docker.com/get-docker/)
- [golang-migrate](https://github.com/golang-migrate/migrate) (for manual db migrations)

## 🐳 Getting Started (Docker Compose)

The easiest way to run the project locally is using Docker Compose. This will spin up the Backend, Validator Engine, PostgreSQL, Redis, and RabbitMQ.

1. **Clone the repository and navigate to the backend folder:**
   ```bash
   cd backend
   ```

2. **Start the services:**
   ```bash
   docker-compose up -d
   ```

3. **Verify the services are running:**
   ```bash
   docker-compose ps
   ```

   The backend API will be accessible at `http://localhost:8080`.

## 💻 Local Development (Native)

If you prefer to run the Go application natively for development/debugging:

1. **Start the infrastructure dependencies:**
   ```bash
   docker-compose up -d postgres redis rabbitmq
   ```

2. **Configure Environment Variables:**
   Copy the example environment file or create a `.env` file in the root of the `backend` directory.

   ```env
   POSTGRES_URL=postgres://postgres:postgrespassword@localhost:5432/depin_db?sslmode=disable
   REDIS_URL=localhost:6379
   RABBITMQ_URL=amqp://guest:guest@localhost:5672/
   VALIDATOR_ADDR=localhost:50052
   JWT_SECRET=super_secret_key_change_me_in_production
   SERVER_PORT=8080
   ```

3. **Run Database Migrations:**
   Ensure you have `golang-migrate` CLI installed.
   ```bash
   migrate -path migrations -database "postgres://postgres:postgrespassword@localhost:5432/depin_db?sslmode=disable" up
   ```

4. **Run the Validator Engine (Optional but required for full functionality):**
   ```bash
   cd validator-engine
   go run main.go
   ```

5. **Run the Main Server:**
   ```bash
   go run cmd/server/main.go
   ```

## 📦 Database Migrations

This project uses `golang-migrate` for database schema management. Migration files are located in the `migrations/` directory.

- **Create a new migration:**
  ```bash
  migrate create -ext sql -dir migrations -seq migration_name
  ```
- **Apply migrations (Up):**
  ```bash
  migrate -path migrations -database "$POSTGRES_URL" up
  ```
- **Rollback migrations (Down):**
  ```bash
  migrate -path migrations -database "$POSTGRES_URL" down 1
  ```

## 🤝 Contributing

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## 📄 License

This project is licensed under the MIT License - see the LICENSE file for details.
