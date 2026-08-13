package classifier

import (
	"fmt"

	hanconv "github.com/fhluo/hanconv/go"
)

// ToTraditional 把简体中文转为繁体。
func ToTraditional(text string) (string, error) {
	return hanconv.S2T(text), nil
}

// ToSimplified 把繁体中文转为简体。
func ToSimplified(text string) (string, error) {
	return hanconv.T2S(text), nil
}

// ToTraditionalArray 批量把字符串转为繁体。
func ToTraditionalArray(texts []string) ([]string, error) {
	result := make([]string, len(texts))
	for i, text := range texts {
		converted, err := ToTraditional(text)
		if err != nil {
			return nil, fmt.Errorf("failed to convert text at index %d: %w", i, err)
		}
		result[i] = converted
	}
	return result, nil
}

// ToSimplifiedArray 批量把字符串转为简体。
func ToSimplifiedArray(texts []string) ([]string, error) {
	result := make([]string, len(texts))
	for i, text := range texts {
		converted, err := ToSimplified(text)
		if err != nil {
			return nil, fmt.Errorf("failed to convert text at index %d: %w", i, err)
		}
		result[i] = converted
	}
	return result, nil
}

// ToTraditionalPointer 把字符串指针指向的内容转为繁体，nil 或空串原样返回。
func ToTraditionalPointer(text *string) (*string, error) {
	if text == nil || *text == "" {
		return text, nil
	}
	converted, err := ToTraditional(*text)
	if err != nil {
		return nil, err
	}
	return &converted, nil
}

// ConvertPoemToTraditional 把一首诗词的各个字段统一转为繁体。
func ConvertPoemToTraditional(title, author, content, rhythmic string) (string, string, string, string, error) {
	t, err := ToTraditional(title)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to convert title: %w", err)
	}

	a, err := ToTraditional(author)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to convert author: %w", err)
	}

	c, err := ToTraditional(content)
	if err != nil {
		return "", "", "", "", fmt.Errorf("failed to convert content: %w", err)
	}

	r := rhythmic
	if rhythmic != "" {
		r, err = ToTraditional(rhythmic)
		if err != nil {
			return "", "", "", "", fmt.Errorf("failed to convert rhythmic: %w", err)
		}
	}

	return t, a, c, r, nil
}
