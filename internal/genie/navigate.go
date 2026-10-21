package genie

import (
	"strings"

	"asi-genie-poc/internal/cards"
)

const (
	navigateActionGoThere         = "Go there"
	navigateActionOpenReport      = "Open report"
	navigateActionOpenReservation = "Open reservation"
)

func navigateScreen(intentID, title, action string, link cards.Link) cards.Card {
	if strings.TrimSpace(title) == "" {
		title = linkScreenTitle(link)
	}
	if strings.TrimSpace(action) == "" {
		action = navigateActionGoThere
	}
	link.Label = action
	return cards.Navigate(intentID, title, link)
}

func linkScreenTitle(link cards.Link) string {
	return strings.TrimPrefix(strings.TrimSpace(link.Label), "Open ")
}

func (r routeDef) navigateCard(intentID string, pid int, params map[string]string) cards.Card {
	title := r.feature
	if title == "" {
		title = strings.TrimPrefix(r.label, "Open ")
	}
	return navigateScreen(intentID, title, navigateActionGoThere, cards.Link{
		Route:  p(pid, r.suffix),
		Params: params,
	})
}

func navigateFromModuleLink(intentID, subtitle string, link cards.Link) cards.Card {
	c := navigateScreen(intentID, linkScreenTitle(link), navigateActionGoThere, link)
	if subtitle != "" {
		c.Subtitle = subtitle
	}
	return c
}

func navigateLinkAction(link cards.Link) cards.Link {
	link.Label = navigateActionGoThere
	return link
}
