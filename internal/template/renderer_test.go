package template

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderSimpleVariable(t *testing.T) {
	renderer := NewRenderer()

	result, err := renderer.Render("Hello {{ name }}!", map[string]interface{}{
		"name": "World",
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello World!", result)
}

func TestRenderMultipleVariables(t *testing.T) {
	renderer := NewRenderer()

	result, err := renderer.Render("HOST={{ host }}\nPORT={{ port }}", map[string]interface{}{
		"host": "db.example.com",
		"port": "5432",
	})
	require.NoError(t, err)
	assert.Equal(t, "HOST=db.example.com\nPORT=5432", result)
}

func TestRenderForLoop(t *testing.T) {
	renderer := NewRenderer()

	template := `{% for item in items %}{{ item }}
{% endfor %}`

	result, err := renderer.Render(template, map[string]interface{}{
		"items": []string{"a", "b", "c"},
	})
	require.NoError(t, err)
	assert.Contains(t, result, "a")
	assert.Contains(t, result, "b")
	assert.Contains(t, result, "c")
}

func TestRenderForLoopWithObjects(t *testing.T) {
	renderer := NewRenderer()

	// Gonja iterates over map keys with {% for key in dict %}
	// and accesses values via dict[key] or dict.key
	template := `{% for ep in endpoints %}# {{ ep.name }}
url={{ ep.data.url }}
{% endfor %}`

	endpoints := []map[string]interface{}{
		{
			"name": "svc-a",
			"data": map[string]interface{}{
				"url": "http://a.example.com",
			},
		},
		{
			"name": "svc-b",
			"data": map[string]interface{}{
				"url": "http://b.example.com",
			},
		},
	}

	result, err := renderer.Render(template, map[string]interface{}{
		"endpoints": endpoints,
	})
	require.NoError(t, err)
	assert.Contains(t, result, "# svc-a")
	assert.Contains(t, result, "# svc-b")
	assert.Contains(t, result, "url=http://a.example.com")
	assert.Contains(t, result, "url=http://b.example.com")
}

func TestRenderConditional(t *testing.T) {
	renderer := NewRenderer()

	template := `{% if enabled %}feature=on{% else %}feature=off{% endif %}`

	result, err := renderer.Render(template, map[string]interface{}{
		"enabled": true,
	})
	require.NoError(t, err)
	assert.Equal(t, "feature=on", result)

	result, err = renderer.Render(template, map[string]interface{}{
		"enabled": false,
	})
	require.NoError(t, err)
	assert.Equal(t, "feature=off", result)
}

func TestRenderFilter(t *testing.T) {
	renderer := NewRenderer()

	result, err := renderer.Render("{{ name|upper }}", map[string]interface{}{
		"name": "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "HELLO", result)
}

func TestRenderEmptyContext(t *testing.T) {
	renderer := NewRenderer()

	result, err := renderer.Render("Static content only", map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "Static content only", result)
}

func TestRenderInvalidTemplateSyntax(t *testing.T) {
	renderer := NewRenderer()

	_, err := renderer.Render("{% invalid_tag %}", map[string]interface{}{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to compile template")
}

func TestRenderConfigMapLikeOutput(t *testing.T) {
	renderer := NewRenderer()

	template := `DATABASE_HOST={{ db_host }}
DATABASE_PASSWORD={{ db_password }}
{% for ep in endpoints %}# {{ ep.name }}
url={{ ep.data.url }}
port={{ ep.data.port }}
{% endfor %}`

	context := map[string]interface{}{
		"db_host":     "db.example.com",
		"db_password": "s3cret",
		"endpoints": []map[string]interface{}{
			{
				"name": "api-server",
				"data": map[string]interface{}{
					"url":  "https://api.example.com",
					"port": "443",
				},
			},
		},
	}

	result, err := renderer.Render(template, context)
	require.NoError(t, err)
	assert.Contains(t, result, "DATABASE_HOST=db.example.com")
	assert.Contains(t, result, "DATABASE_PASSWORD=s3cret")
	assert.Contains(t, result, "# api-server")
	assert.Contains(t, result, "url=https://api.example.com")
	assert.Contains(t, result, "port=443")
}

func TestValidateValidTemplate(t *testing.T) {
	renderer := NewRenderer()

	err := renderer.Validate("Hello {{ name }}!")
	assert.NoError(t, err)
}

func TestValidateInvalidTemplate(t *testing.T) {
	renderer := NewRenderer()

	err := renderer.Validate("{% invalid_block %}")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "template syntax error")
}
