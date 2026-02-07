// Package template provides Jinja template rendering using Gonja.
package template

import (
	"fmt"

	"github.com/guided-traffic/gonja"
)

// Renderer renders Jinja-like templates using the Gonja engine.
type Renderer struct{}

// NewRenderer creates a new template Renderer.
func NewRenderer() *Renderer {
	return &Renderer{}
}

// Render compiles and executes a Jinja template string with the given context.
// Returns the rendered output string or an error if compilation/execution fails.
func (r *Renderer) Render(templateStr string, context map[string]interface{}) (string, error) {
	tpl, err := gonja.FromString(templateStr)
	if err != nil {
		return "", fmt.Errorf("failed to compile template: %w", err)
	}

	out, err := tpl.Execute(gonja.Context(context))
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return out, nil
}

// Validate checks if a template string is syntactically valid without executing it.
func (r *Renderer) Validate(templateStr string) error {
	_, err := gonja.FromString(templateStr)
	if err != nil {
		return fmt.Errorf("template syntax error: %w", err)
	}
	return nil
}
