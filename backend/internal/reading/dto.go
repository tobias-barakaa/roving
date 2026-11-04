// internal/reading/dto.go
package reading

import "time"

type CreateMaterialRequest struct {
	Title       string  `json:"title"       binding:"required,min=1,max=200"`
	Kind        string  `json:"kind"        binding:"omitempty,oneof=book article paper video link other"`
	Author      string  `json:"author"      binding:"max=200"`
	URL         *string `json:"url"         binding:"omitempty,url,max=2000"`
	Description string  `json:"description" binding:"max=2000"`
	Unit        string  `json:"unit"        binding:"omitempty,oneof=pages chapters minutes sections"`
	TotalUnits  *int    `json:"totalUnits"  binding:"omitempty,min=1,max=1000000"`
}

type UpdateMaterialRequest struct {
	Title       *string `json:"title"       binding:"omitempty,min=1,max=200"`
	Author      *string `json:"author"      binding:"omitempty,max=200"`
	URL         *string `json:"url"         binding:"omitempty,url,max=2000"`
	Description *string `json:"description" binding:"omitempty,max=2000"`
	TotalUnits  *int    `json:"totalUnits"  binding:"omitempty,min=1,max=1000000"`
}

// UpdateProgressRequest: send currentUnit, status, or both.
//   - currentUnit alone: status is derived (0 → not_started, >0 → reading,
//     reaching totalUnits → finished).
//   - status "finished" jumps to totalUnits when the total is known.
//   - status "not_started" resets progress to 0.
type UpdateProgressRequest struct {
	CurrentUnit *int    `json:"currentUnit" binding:"omitempty,min=0,max=1000000"`
	Status      *string `json:"status"      binding:"omitempty,oneof=not_started reading finished"`
}

type MaterialResponse struct {
	ID          string     `json:"id"`
	GroupID     string     `json:"groupId"`
	AddedBy     string     `json:"addedBy"`
	Title       string     `json:"title"`
	Kind        string     `json:"kind"`
	Author      string     `json:"author"`
	URL         *string    `json:"url,omitempty"`
	Description string     `json:"description"`
	Unit        string     `json:"unit"`
	TotalUnits  *int       `json:"totalUnits,omitempty"`
	ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type MaterialViewResponse struct {
	MaterialResponse
	MyUnit   int    `json:"myUnit"`
	MyStatus string `json:"myStatus"`
	Readers  int    `json:"readers"`
	Finished int    `json:"finished"`
}

type ProgressResponse struct {
	MaterialID  string     `json:"materialId"`
	CurrentUnit int        `json:"currentUnit"`
	Status      string     `json:"status"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type RosterEntryResponse struct {
	UserID      string     `json:"userId"`
	DisplayName string     `json:"displayName"`
	CurrentUnit int        `json:"currentUnit"`
	Status      string     `json:"status"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

func toMaterialResponse(m *Material) MaterialResponse {
	return MaterialResponse{
		ID: m.ID, GroupID: m.GroupID, AddedBy: m.AddedBy, Title: m.Title,
		Kind: m.Kind, Author: m.Author, URL: m.URL, Description: m.Description,
		Unit: m.Unit, TotalUnits: m.TotalUnits, ArchivedAt: m.ArchivedAt,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

func toMaterialViewResponse(v *MaterialView) MaterialViewResponse {
	return MaterialViewResponse{
		MaterialResponse: toMaterialResponse(&v.Material),
		MyUnit:           v.MyUnit,
		MyStatus:         v.MyStatus,
		Readers:          v.Readers,
		Finished:         v.Finished,
	}
}

func toProgressResponse(p *Progress) ProgressResponse {
	return ProgressResponse{
		MaterialID: p.MaterialID, CurrentUnit: p.CurrentUnit, Status: p.Status,
		StartedAt: p.StartedAt, FinishedAt: p.FinishedAt, UpdatedAt: p.UpdatedAt,
	}
}