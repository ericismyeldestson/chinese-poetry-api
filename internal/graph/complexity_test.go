package graph

import (
	"context"
	"testing"

	"github.com/99designs/gqlgen/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/graph/generated"
)

func TestComplexityRootPricesPaginatedRows(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{Complexity: ComplexityRoot()})
	document, err := parser.ParseQuery(&ast.Source{Input: `query { poems(pageSize: 100) { edges { node { title content } } totalCount } }`})
	require.NoError(t, err)
	require.Empty(t, validator.ValidateWithRules(schema.Schema(), document, nil))

	cost := complexity.Calculate(context.Background(), schema, document.Operations[0], nil)
	assert.GreaterOrEqual(t, cost, 400, "100 rows must not have the same cost as one result object")
}

func TestNestedAuthorPoemsCannotFanOutUnderDefaultBudget(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{Complexity: ComplexityRoot()})
	document, err := parser.ParseQuery(&ast.Source{Input: `query {
		authors(pageSize: 100) { edges { node { poems(pageSize: 1) { totalCount } } } }
	}`})
	require.NoError(t, err)
	require.Empty(t, validator.ValidateWithRules(schema.Schema(), document, nil))

	cost := complexity.Calculate(context.Background(), schema, document.Operations[0], nil)
	assert.Greater(t, cost, 1000, "nested author poems fan-out must exceed the default complexity limit")
}

func TestDirectAuthorPoemsFitsDefaultBudget(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{Complexity: ComplexityRoot()})
	document, err := parser.ParseQuery(&ast.Source{Input: `query {
		author(id: "1") { poems(pageSize: 1) { totalCount } }
	}`})
	require.NoError(t, err)
	require.Empty(t, validator.ValidateWithRules(schema.Schema(), document, nil))

	cost := complexity.Calculate(context.Background(), schema, document.Operations[0], nil)
	assert.LessOrEqual(t, cost, 1000, "direct author poems should remain available at the default limit")
}
