package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	httpVariablePattern   = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_.-]*)\s*\}\}`)
	httpAssignmentPattern = regexp.MustCompile(`^@([A-Za-z_][A-Za-z0-9_.-]*)\s*=\s*(.*)$`)
	httpNamePattern       = regexp.MustCompile(`^(?:#|//)\s*@name\s+([A-Za-z_][A-Za-z0-9_.-]*)\s*$`)
	httpMethodPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]*$`)
)

// ParseHTTPEnvironment parses an http-client environment JSON object. Values
// may be strings, numbers, or booleans. Secrets must use the explicit shape
// {"$secret":"account"}; inline secret values are never accepted.
func ParseHTTPEnvironment(content, environmentName string) (HTTPEnvironment, error) {
	result := HTTPEnvironment{Values: map[string]string{}, SecretRefs: map[string]string{}}
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.UseNumber()
	var root map[string]json.RawMessage
	if err := decoder.Decode(&root); err != nil {
		return result, fmt.Errorf("parse HTTP environment: %w", err)
	}
	selected := root
	if environmentName != "" {
		raw, ok := root[environmentName]
		if !ok {
			return result, fmt.Errorf("HTTP environment %q was not found", environmentName)
		}
		var named map[string]json.RawMessage
		if err := json.Unmarshal(raw, &named); err != nil {
			return result, fmt.Errorf("HTTP environment %q must be an object", environmentName)
		}
		selected = named
	} else if looksLikeNamedHTTPEnvironments(root) {
		return result, fmt.Errorf("HTTP environment name is required")
	}

	for name, raw := range selected {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return result, fmt.Errorf("environment variable %q: %w", name, err)
		}
		switch typed := value.(type) {
		case string:
			result.Values[name] = typed
		case json.Number:
			result.Values[name] = typed.String()
		case bool:
			result.Values[name] = strconv.FormatBool(typed)
		case map[string]any:
			ref, ok := typed["$secret"].(string)
			if !ok || strings.TrimSpace(ref) == "" || len(typed) != 1 {
				return result, fmt.Errorf("environment variable %q must use {\"$secret\":\"account\"} for secret values", name)
			}
			result.SecretRefs[name] = strings.TrimSpace(ref)
		default:
			return result, fmt.Errorf("environment variable %q must be a primitive value or a $secret reference", name)
		}
	}
	return result, nil
}

func looksLikeNamedHTTPEnvironments(root map[string]json.RawMessage) bool {
	for _, raw := range root {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) == nil {
			if _, isSecret := object["$secret"]; !isSecret {
				return true
			}
		}
	}
	return false
}

// ParseHTTPFile parses the request blocks in a .http document. File-level @x
// assignments override environment values. Secret variables remain opaque
// placeholders and are resolved only by HTTPClientService.SendRequest.
func ParseHTTPFile(content string, environment HTTPEnvironment) ([]HTTPRequest, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	lines := strings.Split(content, "\n")
	values := cloneStringMap(environment.Values)
	secretRefs := cloneStringMap(environment.SecretRefs)

	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		match := httpAssignmentPattern.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		if _, secret := secretRefs[match[1]]; secret {
			delete(secretRefs, match[1])
		}
		value, err := expandHTTPVariables(match[2], values, secretRefs)
		if err != nil {
			return nil, fmt.Errorf("line %d variable %q: %w", index+1, match[1], err)
		}
		values[match[1]] = value
	}

	type segment struct {
		start int
		end   int
		title string
	}
	segments := make([]segment, 0, 4)
	start, title := 0, ""
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "###") {
			continue
		}
		segments = append(segments, segment{start: start, end: index, title: title})
		start = index + 1
		title = strings.TrimSpace(strings.TrimPrefix(trimmed, "###"))
	}
	segments = append(segments, segment{start: start, end: len(lines), title: title})

	requests := make([]HTTPRequest, 0, len(segments))
	for _, part := range segments {
		request, ok, err := parseHTTPRequestSegment(lines, part.start, part.end, part.title, values, secretRefs)
		if err != nil {
			return nil, err
		}
		if ok {
			requests = append(requests, request)
		}
	}
	if len(requests) == 0 && strings.TrimSpace(content) != "" {
		return nil, fmt.Errorf("HTTP document contains no requests")
	}
	return requests, nil
}

func parseHTTPRequestSegment(lines []string, start, end int, title string, values, secretRefs map[string]string) (HTTPRequest, bool, error) {
	request := HTTPRequest{Headers: map[string]string{}, SecretRefs: map[string]string{}}
	index := start
	for index < end {
		trimmed := strings.TrimSpace(lines[index])
		if match := httpNamePattern.FindStringSubmatch(trimmed); match != nil {
			request.Name = match[1]
			index++
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") || httpAssignmentPattern.MatchString(trimmed) {
			index++
			continue
		}
		break
	}
	if index >= end {
		return request, false, nil
	}
	request.StartLine = index + 1
	request.EndLine = end
	if request.Name == "" {
		request.Name = title
	}

	requestLine, err := expandHTTPVariables(strings.TrimSpace(lines[index]), values, secretRefs)
	if err != nil {
		return request, false, fmt.Errorf("line %d request: %w", index+1, err)
	}
	fields := strings.Fields(requestLine)
	if len(fields) == 1 && strings.Contains(fields[0], "://") {
		request.Method, request.URL = "GET", fields[0]
	} else {
		if len(fields) < 2 || len(fields) > 3 || (len(fields) == 3 && !strings.HasPrefix(strings.ToUpper(fields[2]), "HTTP/")) {
			return request, false, fmt.Errorf("line %d: request line must be METHOD URL [HTTP/version]", index+1)
		}
		if !httpMethodPattern.MatchString(fields[0]) {
			return request, false, fmt.Errorf("line %d: invalid HTTP method %q", index+1, fields[0])
		}
		request.Method, request.URL = strings.ToUpper(fields[0]), fields[1]
	}
	index++

	bodyStart := -1
	for index < end {
		line := lines[index]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			bodyStart = index + 1
			break
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			index++
			continue
		}
		colon := strings.Index(line, ":")
		if colon <= 0 {
			return request, false, fmt.Errorf("line %d: malformed HTTP header", index+1)
		}
		name := strings.TrimSpace(line[:colon])
		if !httpMethodPattern.MatchString(name) {
			return request, false, fmt.Errorf("line %d: invalid HTTP header name %q", index+1, name)
		}
		value, err := expandHTTPVariables(strings.TrimSpace(line[colon+1:]), values, secretRefs)
		if err != nil {
			return request, false, fmt.Errorf("line %d header %q: %w", index+1, name, err)
		}
		request.Headers[name] = value
		index++
	}
	if bodyStart >= 0 && bodyStart < end {
		body := strings.TrimSpace(strings.Join(lines[bodyStart:end], "\n"))
		request.Body, err = expandHTTPVariables(body, values, secretRefs)
		if err != nil {
			return request, false, fmt.Errorf("line %d body: %w", bodyStart+1, err)
		}
	}
	request.SecretRefs = collectHTTPSecretRefs(request, secretRefs)
	return request, true, nil
}

func collectHTTPSecretRefs(request HTTPRequest, available map[string]string) map[string]string {
	result := map[string]string{}
	values := append([]string{request.URL, request.Body}, mapValues(request.Headers)...)
	for _, value := range values {
		for _, match := range httpVariablePattern.FindAllStringSubmatch(value, -1) {
			if reference, ok := available[match[1]]; ok {
				result[match[1]] = reference
			}
		}
	}
	return result
}

func expandHTTPVariables(value string, values, secretRefs map[string]string) (string, error) {
	var firstErr error
	for pass := 0; pass < 32; pass++ {
		changed := false
		result := httpVariablePattern.ReplaceAllStringFunc(value, func(match string) string {
			parts := httpVariablePattern.FindStringSubmatch(match)
			name := parts[1]
			if replacement, ok := values[name]; ok {
				changed = true
				return replacement
			}
			if _, ok := secretRefs[name]; ok {
				return "{{" + name + "}}"
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("unresolved variable %q", name)
			}
			return match
		})
		if firstErr != nil {
			return "", firstErr
		}
		value = result
		if !changed {
			return value, nil
		}
	}
	return "", fmt.Errorf("variable expansion exceeded 32 passes (possible cycle)")
}

func cloneStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
