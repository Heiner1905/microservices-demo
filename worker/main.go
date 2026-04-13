package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"

	kingpin "github.com/alecthomas/kingpin/v2"
	"github.com/IBM/sarama"
)

var (
	brokerList        = kingpin.Flag("brokerList", "List of brokers to connect").Default("kafka:9092").Strings()
	topic             = kingpin.Flag("topic", "Topic name").Default("votes").String()
	messageCountStart = kingpin.Flag("messageCountStart", "Message counter start from:").Int()
)

const (
	consumerGroup = "worker-consumer-group" // Competing Consumer: todas las instancias comparten este group ID
)

// ConsumerGroupHandler implementa sarama.ConsumerGroupHandler
type ConsumerGroupHandler struct {
	db    *sql.DB
	count int64 // contador thread-safe con atomic
}

func (h *ConsumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *ConsumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

func (h *ConsumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for msg := range claim.Messages() {
		atomic.AddInt64(&h.count, 1)

		fmt.Printf("[partition %d | offset %d] user=%s vote=%s\n",
			msg.Partition, msg.Offset, string(msg.Key), string(msg.Value))

		insertStmt := `
			INSERT INTO votes (id, vote)
			VALUES ($1, $2)
			ON CONFLICT (id) DO UPDATE SET vote = $2
		`
		if _, err := h.db.Exec(insertStmt, string(msg.Key), string(msg.Value)); err != nil {
			log.Printf("Error inserting vote (user=%s): %v", string(msg.Key), err)
			// No hacemos panic: logueamos y seguimos consumiendo
			continue
		}

		// Solo marcamos el mensaje como procesado si el insert fue exitoso
		session.MarkMessage(msg, "")
	}
	return nil
}

func main() {
	kingpin.Parse()

	// Conexión a la base de datos
	db := openDatabase()
	defer db.Close()

	pingDatabase(db)
	initSchema(db)

	// Conexión al consumer group de Kafka
	cg := getKafkaConsumerGroup()
	defer cg.Close()

	handler := &ConsumerGroupHandler{db: db}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown al recibir SIGINT (Ctrl+C o señal de Kubernetes)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	go func() {
		<-signals
		fmt.Println("Señal de interrupción recibida, apagando worker...")
		cancel()
	}()

	fmt.Printf("Worker iniciado. Consumer group: %s | Topic: %s\n", consumerGroup, *topic)

	// Loop principal de consumo — Kafka redistribuye particiones automáticamente
	// entre todas las instancias del consumer group (patrón Competing Consumer)
	for {
		if err := cg.Consume(ctx, []string{*topic}, handler); err != nil {
			log.Printf("Error en consumo: %v", err)
		}
		if ctx.Err() != nil {
			break
		}
	}

	log.Printf("Worker apagado. Mensajes procesados en esta instancia: %d", atomic.LoadInt64(&handler.count))
}

// openDatabase abre la conexión a PostgreSQL usando variables de entorno.
// Hace reintentos con backoff para tolerar arranque lento del contenedor.
func openDatabase() *sql.DB {
	host     := getEnv("DB_HOST", "postgresql")
	port     := getEnv("DB_PORT", "5432")
	user     := getEnv("DB_USER", "okteto")
	password := getEnv("DB_PASSWORD", "okteto") // cambiar si es diferente
	dbname   := getEnv("DB_NAME", "votes")

	psqlconn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, dbname,
	)

	db, err := sql.Open("postgres", psqlconn)
	if err != nil {
		log.Fatalf("Error abriendo conexión a DB: %v", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	return db
}

// pingDatabase verifica la conexión real con reintentos y backoff.
func pingDatabase(db *sql.DB) {
	fmt.Println("Esperando a PostgreSQL...")
	for i := 1; i <= 30; i++ {
		if err := db.Ping(); err == nil {
			fmt.Println("PostgreSQL conectado!")
			return
		}
		fmt.Printf("Intento %d/30 fallido, reintentando en 2s...\n", i)
		time.Sleep(2 * time.Second)
	}
	log.Fatal("No se pudo conectar a PostgreSQL después de 30 intentos")
}

// initSchema crea la tabla de votos si no existe.
// NO hace DROP TABLE para que múltiples réplicas puedan arrancar sin pisarse.
func initSchema(db *sql.DB) {
	createTableStmt := `
		CREATE TABLE IF NOT EXISTS votes (
			id   VARCHAR(255) NOT NULL UNIQUE,
			vote VARCHAR(255) NOT NULL
		)
	`
	if _, err := db.Exec(createTableStmt); err != nil {
		log.Fatalf("Error creando tabla votes: %v", err)
	}
	fmt.Println("Esquema de base de datos verificado.")
}

// getKafkaConsumerGroup crea el consumer group con RoundRobin para distribuir
// particiones equitativamente entre todas las instancias (Competing Consumer).
func getKafkaConsumerGroup() sarama.ConsumerGroup {
	config := sarama.NewConfig()
	config.Consumer.Return.Errors = true
	config.Consumer.Offsets.Initial = sarama.OffsetNewest

	// RoundRobin distribuye particiones entre instancias del mismo consumer group
	config.Consumer.Group.Rebalance.GroupStrategies = []sarama.BalanceStrategy{
		sarama.NewBalanceStrategyRoundRobin(),
	}

	brokers := *brokerList
	fmt.Printf("Esperando a Kafka en %v...\n", brokers)

	for i := 1; ; i++ {
		cg, err := sarama.NewConsumerGroup(brokers, consumerGroup, config)
		if err == nil {
			fmt.Printf("Kafka conectado! Consumer group: %s\n", consumerGroup)
			return cg
		}
		log.Printf("Intento %d fallido conectando a Kafka: %v. Reintentando en 3s...", i, err)
		time.Sleep(3 * time.Second)
	}
}

// getEnv retorna el valor de una variable de entorno o un valor por defecto.
func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
