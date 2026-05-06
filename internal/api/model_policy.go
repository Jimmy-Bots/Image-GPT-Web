package api

import (
	"context"
	"fmt"
	"strings"

	"gpt-image-web/internal/domain"
)

const defaultImageWorkbenchModel = "gpt-image-2"
const defaultImageMaxCount = 4

func (s *Server) imageWorkbenchModel(ctx context.Context) string {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return defaultImageWorkbenchModel
	}
	model := strings.TrimSpace(stringFromAny(settings["image_workbench_model"], ""))
	if model == "" {
		return defaultImageWorkbenchModel
	}
	return model
}

func (s *Server) imageMaxCount(ctx context.Context) int {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return defaultImageMaxCount
	}
	limit := intMapValue(settings, "image_max_count")
	if limit < 1 {
		return defaultImageMaxCount
	}
	return limit
}

func (s *Server) allowedPublicModels(ctx context.Context) []string {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return nil
	}
	return allowedPublicModelsFromSettings(settings)
}

func allowedPublicModelsFromSettings(settings map[string]any) []string {
	raw := settings["allowed_public_models"]
	if raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			value := strings.TrimSpace(stringFromAny(item, ""))
			if value != "" {
				out = append(out, value)
			}
		}
		return compactStrings(out)
	case []string:
		return compactStrings(typed)
	case string:
		lines := strings.Split(typed, "\n")
		return compactStrings(lines)
	default:
		return nil
	}
}

func (s *Server) enforceImageRequestModel(ctx context.Context, identity Identity, requested string) (string, error) {
	model := strings.TrimSpace(requested)
	if identity.AuthType == "session" {
		return s.imageWorkbenchModel(ctx), nil
	}
	allowed := s.allowedPublicModels(ctx)
	if len(allowed) == 0 {
		if model == "" {
			return defaultImageWorkbenchModel, nil
		}
		return model, nil
	}
	if model == "" {
		return allowed[0], nil
	}
	for _, item := range allowed {
		if item == model {
			return model, nil
		}
	}
	return "", fmt.Errorf("model %q is not allowed", model)
}

func (s *Server) enforceGeneralRequestModel(ctx context.Context, requested string) (string, error) {
	model := strings.TrimSpace(requested)
	allowed := s.allowedPublicModels(ctx)
	if len(allowed) == 0 || model == "" {
		if model == "" {
			return "auto", nil
		}
		return model, nil
	}
	for _, item := range allowed {
		if item == model {
			return model, nil
		}
	}
	return "", fmt.Errorf("model %q is not allowed", model)
}

func filterModelsByAllowed(result map[string]any, allowed []string) map[string]any {
	if len(allowed) == 0 {
		return result
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, item := range allowed {
		allowedSet[item] = struct{}{}
	}
	rawItems, _ := result["data"].([]map[string]any)
	if rawItems == nil {
		if values, ok := result["data"].([]any); ok {
			rawItems = make([]map[string]any, 0, len(values))
			for _, value := range values {
				if item, ok := value.(map[string]any); ok {
					rawItems = append(rawItems, item)
				}
			}
		}
	}
	filtered := make([]map[string]any, 0, len(rawItems))
	for _, item := range rawItems {
		id := strings.TrimSpace(stringFromAny(item["id"], ""))
		if id == "" {
			continue
		}
		if _, ok := allowedSet[id]; ok {
			filtered = append(filtered, item)
		}
	}
	result["data"] = filtered
	return result
}

func modelPolicyForIdentity(ctx context.Context, identity Identity, settings map[string]any) map[string]any {
	config := map[string]any{
		"workbench_model":       defaultImageWorkbenchModel,
		"image_max_count":       defaultImageMaxCount,
		"allowed_public_models": []string{},
	}
	if model := strings.TrimSpace(stringFromAny(settings["image_workbench_model"], "")); model != "" {
		config["workbench_model"] = model
	}
	if limit := intMapValue(settings, "image_max_count"); limit > 0 {
		config["image_max_count"] = limit
	}
	allowed := allowedPublicModelsFromSettings(settings)
	if len(allowed) > 0 {
		config["allowed_public_models"] = allowed
	}
	if identity.Role == domain.RoleAdmin {
		config["is_admin"] = true
	}
	return config
}
