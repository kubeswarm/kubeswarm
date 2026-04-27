/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package openai

import (
	"testing"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

func TestSortOpenAITools_OrdersByName(t *testing.T) {
	tools := []openaisdk.ChatCompletionToolParam{
		{Function: shared.FunctionDefinitionParam{Name: "gamma"}},
		{Function: shared.FunctionDefinitionParam{Name: "alpha"}},
		{Function: shared.FunctionDefinitionParam{Name: "beta"}},
	}
	sortOpenAITools(tools)
	want := []string{"alpha", "beta", "gamma"}
	for i, w := range want {
		if tools[i].Function.Name != w {
			t.Errorf("tools[%d].Name = %q, want %q", i, tools[i].Function.Name, w)
		}
	}
}

func TestSortOpenAITools_AlreadySorted(t *testing.T) {
	tools := []openaisdk.ChatCompletionToolParam{
		{Function: shared.FunctionDefinitionParam{Name: "a"}},
		{Function: shared.FunctionDefinitionParam{Name: "b"}},
		{Function: shared.FunctionDefinitionParam{Name: "c"}},
	}
	sortOpenAITools(tools)
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if tools[i].Function.Name != w {
			t.Errorf("tools[%d].Name = %q, want %q", i, tools[i].Function.Name, w)
		}
	}
}

func TestSortOpenAITools_Empty(t *testing.T) {
	var tools []openaisdk.ChatCompletionToolParam
	sortOpenAITools(tools) // should not panic
	if len(tools) != 0 {
		t.Errorf("expected empty slice, got %d", len(tools))
	}
}

func TestSortOpenAITools_SingleTool(t *testing.T) {
	tools := []openaisdk.ChatCompletionToolParam{
		{Function: shared.FunctionDefinitionParam{Name: "only"}},
	}
	sortOpenAITools(tools)
	if tools[0].Function.Name != "only" {
		t.Errorf("tools[0].Name = %q, want %q", tools[0].Function.Name, "only")
	}
}

func TestSortOpenAITools_DuplicateNames(t *testing.T) {
	tools := []openaisdk.ChatCompletionToolParam{
		{Function: shared.FunctionDefinitionParam{Name: "beta"}},
		{Function: shared.FunctionDefinitionParam{Name: "alpha"}},
		{Function: shared.FunctionDefinitionParam{Name: "alpha"}},
	}
	sortOpenAITools(tools)
	want := []string{"alpha", "alpha", "beta"}
	for i, w := range want {
		if tools[i].Function.Name != w {
			t.Errorf("tools[%d].Name = %q, want %q", i, tools[i].Function.Name, w)
		}
	}
}
