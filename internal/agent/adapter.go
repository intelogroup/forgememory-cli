package agent

type ClientAdapter interface {
	Name() string
	Detect(home string) bool
	Setup(home string) (string, error)
}

func registeredAdapters() []ClientAdapter {
	return []ClientAdapter{
		claudeAdapter{},
		geminiAdapter{},
		codexAdapter{},
		opencodeAdapter{},
		antigravityAdapter{},
	}
}
