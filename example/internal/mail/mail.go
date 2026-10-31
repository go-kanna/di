// Package mail is the domain of the mail the shop sends.
package mail

import "context"

// Message is a mail to a user.
type Message struct {
	To      string
	Subject string
}

// Sender sends mail. As with user.Repository, the domain declares it, the
// infrastructure implements it, and the composition root says which
// implementation serves.
type Sender interface {
	Send(ctx context.Context, m Message) error
}
