package spanner

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	spannerClient "cloud.google.com/go/spanner"
	spannerAdmin "cloud.google.com/go/spanner/admin/database/apiv1"
	"github.com/Cidan/sheep/database"
	"github.com/Cidan/sheep/stats"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"google.golang.org/api/option"
	adminpb "google.golang.org/genproto/googleapis/spanner/admin/database/v1"
	"google.golang.org/grpc/codes"
)

type Spanner struct {
	client *spannerClient.Client
	admin  *spannerAdmin.DatabaseAdminClient
}

// SetupSpanner initializes the spanner clients.
func New(project, instance, db string, opts ...option.ClientOption) (database.Database, error) {
	ctx := context.Background()
	sp := &Spanner{}

	adminClient, err := spannerAdmin.NewDatabaseAdminClient(ctx, opts...)
	if err != nil {
		return nil, err
	}

	sp.admin = adminClient

	// Create the databases if they don't exist.
	err = sp.createSpannerDatabase(ctx, project, instance, db)

	if err != nil {
		return nil, err
	}

	dbstr := fmt.Sprintf("projects/%s/instances/%s/databases/%s",
		project,
		instance,
		db)

	client, err := spannerClient.NewClient(context.Background(), dbstr, opts...)
	if err != nil {
		return nil, err
	}

	sp.client = client
	return sp, err
}

func (s *Spanner) Read(msg *database.Message) error {

	stmt := spannerClient.NewStatement(`
			SELECT SUM(a.Count) as Count
			FROM sheep as a
			WHERE a.Keyspace=@Keyspace
			AND a.Key=@Key
			AND a.Name=@Name
		`)
	stmt.Params["Keyspace"] = msg.Keyspace
	stmt.Params["Key"] = msg.Key
	stmt.Params["Name"] = msg.Name

	// TODO: Expose this stale time.
	// TODO: Stale time breaks tests.
	iter := s.client.
		Single().
		//WithTimestampBound(spanner.MaxStaleness(5*time.Second)).
		Query(context.Background(), stmt)
	defer iter.Stop()

	row, err := iter.Next()

	if err != nil {
		return err
	}

	var value spannerClient.NullInt64
	err = row.ColumnByName("Count", &value)

	if err != nil {
		return err
	}

	if value.Valid {
		msg.Value = value.Int64
		return nil
	}

	return &spannerClient.Error{
		Code: codes.NotFound,
		Desc: "counter not found",
	}
}

func (s *Spanner) Save(message *database.Message) error {
	ctx := context.WithValue(context.Background(), database.ContextKey("message"), message)
	if _, err := s.client.ReadWriteTransaction(ctx, s.doSave); err != nil {
		stats.Incr("spanner.save.error", 1)
		return err
	}
	stats.Incr("spanner.save.success", 1)
	return nil
}

// Here's where the magic happens. Save our message!
func (s *Spanner) doSave(ctx context.Context, rw *spannerClient.ReadWriteTransaction) error {
	msg := ctx.Value(database.ContextKey("message")).(*database.Message)
	shards := viper.GetInt("spanner.shards")
	shard := rand.Intn(shards)

	// First, let's check and see if our message has been written.
	row, err := rw.ReadRow(context.Background(), "sheep_transaction", spannerClient.Key{msg.Keyspace, msg.Key, msg.Name, msg.UUID}, []string{"UUID"})
	if err != nil {
		if spannerClient.ErrCode(err) != codes.NotFound {
			return err
		}
	} else if err == nil {
		// We need to return if err is nil, this means
		// the UUID was found.
		return nil
	}

	// Let's get our current count
	var move int64
	row, err = rw.ReadRow(context.Background(), "sheep", spannerClient.Key{msg.Keyspace, msg.Key, msg.Name, shard}, []string{"Count"})
	if err != nil {
		if spannerClient.ErrCode(err) != codes.NotFound {
			return err
		}
	} else {
		row.ColumnByName("Count", &move)
	}

	// Now we'll do our operation.
	switch msg.Operation {
	case "INCR":
		move++
	case "DECR":
		move--
	case "SET":
		move = msg.Value
	default:
		return &spannerClient.Error{
			Code: codes.InvalidArgument,
			Desc: "Invalid operation sent from message '" + msg.Operation + "', aborting transaction!",
		}
	}

	m := []*spannerClient.Mutation{}

	log.Debug().Int("shard", shard).Msg("shard selected for op")
	if msg.Operation == "SET" {
		for i := 0; i < shards; i++ {
			m = append(m, spannerClient.InsertOrUpdate(
				"sheep",
				[]string{"Keyspace", "Key", "Name", "Shard", "Count"},
				[]interface{}{msg.Keyspace, msg.Key, msg.Name, i, move},
			))
		}
	} else {
		m = append(m, spannerClient.InsertOrUpdate(
			"sheep",
			[]string{"Keyspace", "Key", "Name", "Shard", "Count"},
			[]interface{}{msg.Keyspace, msg.Key, msg.Name, shard, move},
		))
	}

	m = append(m, spannerClient.InsertOrUpdate(
		"sheep_transaction",
		[]string{"Keyspace", "Key", "Name", "UUID", "Time"},
		[]interface{}{msg.Keyspace, msg.Key, msg.Name, msg.UUID, time.Now()}))

	// ...and write!
	return rw.BufferWrite(m)

}

func (s *Spanner) createSpannerDatabase(ctx context.Context, project, instance, db string) error {
	// Create our database if it doesn't exist.
	_, err := s.admin.GetDatabase(ctx, &adminpb.GetDatabaseRequest{
		Name: "projects/" + project + "/instances/" + instance + "/databases/" + db})
	if err != nil {
		// Database doesn't exist, or error.
		op, err := s.admin.CreateDatabase(ctx, &adminpb.CreateDatabaseRequest{
			Parent:          "projects/" + project + "/instances/" + instance,
			CreateStatement: "CREATE DATABASE `" + db + "`",
			ExtraStatements: []string{
				`CREATE TABLE sheep (
							Keyspace 	STRING(MAX) NOT NULL,
							Key 			STRING(MAX) NOT NULL,
							Name			STRING(MAX) NOT NULL,
							Shard     INT64       NOT NULL,
							Count 		INT64       NOT NULL
					) PRIMARY KEY (Keyspace, Key, Name, Shard)`,
				`CREATE TABLE sheep_transaction (
							Keyspace 	STRING(MAX) NOT NULL,
							Key 			STRING(MAX) NOT NULL,
							Name			STRING(MAX) NOT NULL,
							UUID 			STRING(128) NOT NULL,
							Time      TIMESTAMP   NOT NULL
					) PRIMARY KEY (Keyspace, Key, Name, UUID)`,
				`CREATE TABLE sheep_stats (
					   UUID      STRING(MAX) NOT NULL,
						 Key       STRING(MAX) NOT NULL,
						 Value     FLOAT64     NOT NULL,
						 Hostname  STRING(MAX) NOT NULL,
						 Last      TIMESTAMP   NOT NULL
				 ) PRIMARY KEY (UUID, Key)`,
			},
		})

		if err != nil {
			return err
		}

		_, err = op.Wait(ctx)

		if err != nil {
			return err
		}
	}
	return nil
}
