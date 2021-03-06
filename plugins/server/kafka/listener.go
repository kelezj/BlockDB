package kafka

import (
	"context"
	"github.com/annchain/BlockDB/backends"
	"github.com/annchain/BlockDB/processors"
	"github.com/segmentio/kafka-go"
	"github.com/sirupsen/logrus"
	"strings"
	"sync"
	"time"
)

type KafkaProcessorConfig struct {
	Topic   string
	Address string
}

type KafkaListener struct {
	config        KafkaProcessorConfig
	ledgerWriter  backends.LedgerWriter
	dataProcessor processors.DataProcessor

	wg      sync.WaitGroup
	stopped bool
}

func (k *KafkaListener) Name() string {
	return "KafkaListener"
}

func NewKafkaListener(config KafkaProcessorConfig, dataProcessor processors.DataProcessor, ledgerWriter backends.LedgerWriter) *KafkaListener {
	return &KafkaListener{
		config:        config,
		ledgerWriter:  ledgerWriter,
		dataProcessor: dataProcessor,
	}
}

func (k *KafkaListener) Start() {
	ps, _ := kafka.LookupPartitions(context.Background(), "tcp", k.config.Address, k.config.Topic)

	// currently we will listen to all partitions
	for _, p := range ps {
		k.wg.Add(1)
		go k.doListen(p)
	}
	logrus.Info("KafkaListener started")
}

func (k *KafkaListener) Stop() {
	k.stopped = true
	k.wg.Wait()
	logrus.Info("KafkaListener stopped")
}

func (k *KafkaListener) doListen(partition kafka.Partition) {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   strings.Split(k.config.Address, ";"),
		Topic:     k.config.Topic,
		Partition: partition.ID,
		MinBytes:  1,    // 1B
		MaxBytes:  10e6, // 10MB
	})
	defer func() {
		_ = r.Close()
		k.wg.Done()
	}()

	deadlineContext, _ := context.WithDeadline(context.Background(), time.Now().Add(time.Second*3))
	err := r.SetOffsetAt(deadlineContext, time.Now())
	if err != nil {
		logrus.WithError(err).WithField("partition", partition).Error("cannot set offset to partition")
		return
	}
	logrus.WithField("partition", partition.ID).WithField("topic", k.config.Topic).Info("kafka partition consumer started")

	for !k.stopped {
		m, err := r.ReadMessage(context.Background())
		if err != nil {
			logrus.WithError(err).WithField("partition", partition.ID).Error("partition error")
			time.Sleep(time.Second * 1)
			continue
		}
		s := string(m.Value)
		logrus.WithFields(logrus.Fields{
			"partition": m.Partition,
			"offset":    m.Offset,
			"msg":       s,
		}).Info("message")

		events := k.dataProcessor.ParseCommand(m.Value)
		for _, event := range events {
			k.ledgerWriter.EnqueueSendToLedger(event)
		}
	}

}
