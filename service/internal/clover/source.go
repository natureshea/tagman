package clover

import (
	"context"
	"time"

	"inktags/internal/items"
)

// Source adapts Client to items.Source.
type Source struct {
	c *Client
}

func AsSource(c *Client) *Source { return &Source{c: c} }

func (s *Source) Name() string { return "clover" }

func toItem(ci Item) items.Item {
	return items.Item{ID: ci.ID, Name: ci.Name, Price: ci.Price}
}

func (s *Source) List(ctx context.Context) ([]items.Item, error) {
	cis, err := s.c.ListItems(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]items.Item, 0, len(cis))
	for _, ci := range cis {
		out = append(out, toItem(ci))
	}
	return out, nil
}

func (s *Source) Get(ctx context.Context, id string) (items.Item, error) {
	ci, err := s.c.GetItem(ctx, id)
	if err != nil {
		return items.Item{}, err
	}
	return toItem(ci), nil
}

func (s *Source) ChangedSince(ctx context.Context, t time.Time) ([]items.Item, error) {
	cis, err := s.c.ChangedSince(ctx, t)
	if err != nil {
		return nil, err
	}
	out := make([]items.Item, 0, len(cis))
	for _, ci := range cis {
		out = append(out, toItem(ci))
	}
	return out, nil
}
