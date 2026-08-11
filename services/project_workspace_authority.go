package services

import "path/filepath"

// GetWorkspaceSnapshot returns the one backend-owned workspace view consumed
// by every renderer window. Callers must treat Generation as the ordering key.
func (p *ProjectService) GetWorkspaceSnapshot() WorkspaceSnapshot {
	if p == nil {
		return WorkspaceSnapshot{Roots: []string{}}
	}
	p.workspaceMu.Lock()
	defer p.workspaceMu.Unlock()
	return p.workspaceSnapshotLocked()
}

func (p *ProjectService) workspaceSnapshotLocked() WorkspaceSnapshot {
	snapshot := WorkspaceSnapshot{Roots: []string{}}
	if p.wsCtx != nil {
		snapshot = p.wsCtx.State()
		if snapshot.Roots == nil {
			snapshot.Roots = []string{}
		}
	}
	if p.activeProject.ID != "" {
		snapshot.ProjectID = p.activeProject.ID
		snapshot.ProjectName = p.activeProject.Name
		snapshot.ProjectPath = p.activeProject.Path
	} else if snapshot.Root != "" {
		snapshot.ProjectName = filepath.Base(snapshot.Root)
		snapshot.ProjectPath = snapshot.Root
	}
	return snapshot
}

func (p *ProjectService) publishWorkspaceSnapshotLocked() {
	snapshot := p.workspaceSnapshotLocked()
	if p.workspaceSnapshotSink != nil {
		p.workspaceSnapshotSink(snapshot)
	}
	if p.app != nil {
		p.app.Event.Emit("workspace:changed", snapshot)
	}
}

func (p *ProjectService) projectMatchesActiveWorkspace(project Project) bool {
	if p.wsCtx == nil {
		return false
	}
	state := p.wsCtx.State()
	if state.Root == "" {
		return false
	}
	if len(project.Roots) > 0 {
		return sameWorkspaceIdentityPath(state.Root, project.Roots[0])
	}
	return sameWorkspaceIdentityPath(state.Root, project.Path)
}
