// Package console is the infrastructure of the shop's mail: a mail.Sender that
// prints what it sends, standing in for a mail service.
package console

import (
	"context"
	"fmt"
	"io"

	"github.com/go-kanna/di/example/internal/mail"
)

// Sender is the mail.Sender printing to a writer.
type Sender struct {
	out io.Writer
}

// NewSender provides a Sender. It returns the struct rather than the
// interface, and the container binds it to mail.Sender.
func NewSender(out io.Writer) *Sender {
	return &Sender{out: out}
}

// Send prints m.
func (s *Sender) Send(_ context.Context, m mail.Message) error {
	if _, err := fmt.Fprintf(s.out, "mail to %s: %s\n", m.To, m.Subject); err != nil {
		return fmt.Errorf("print mail to %s: %w", m.To, err)
	}
	return nil
}
