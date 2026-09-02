package engine

// Module is a compile-time application module. Modules are ordinary Go values:
// the application constructs their dependencies explicitly and the engine only
// gives them the runtime surface needed to register routes, health checks and workers.
type Module interface {
	Name() string
	Register(*Engine) error
}

// ModuleFunc adapts a function into a Module.
type ModuleFunc struct {
	ModuleName   string
	RegisterFunc func(*Engine) error
}

func (m ModuleFunc) Name() string { return m.ModuleName }

func (m ModuleFunc) Register(e *Engine) error {
	if m.RegisterFunc == nil {
		return nil
	}
	return m.RegisterFunc(e)
}
