package main

import "testing"

func TestCurrentInstanceFollowsOwnership(t *testing.T) {
	old := instance{Runtime: "runtime1", ID: "old", State: "running"}
	newInstance := instance{Runtime: "runtime2", ID: "new", State: "paused"}
	for _, tc := range []struct {
		name              string
		owner             *owner
		errors            map[string]string
		wantID, wantState string
	}{
		{"before first backup", nil, nil, "old", "running"},
		{"switched awaiting restore", &owner{Node: "runtime2", State: "ready"}, nil, "", "awaiting_restore"},
		{"restored target", &owner{Node: "runtime2", Instance: "new", State: "active"}, nil, "new", "paused"},
		{"switch back binds original", &owner{Node: "runtime1", Instance: "old", State: "active"}, nil, "old", "running"},
		{"missing owned instance", &owner{Node: "runtime2", Instance: "gone", State: "active"}, nil, "", "missing"},
		{"offline owner", &owner{Node: "runtime2", Instance: "gone", State: "active"}, map[string]string{"runtime2": "offline"}, "", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := logic{OriginNode: "runtime1", OriginID: "old", Owner: tc.owner, Instances: []instance{old, newInstance}}
			row.selectCurrentInstances(tc.errors)
			if row.CurrentState != tc.wantState {
				t.Fatalf("state %s", row.CurrentState)
			}
			if tc.wantID == "" {
				if len(row.CurrentInstances) != 0 {
					t.Fatal("old instance still current")
				}
			} else if len(row.CurrentInstances) != 1 || row.CurrentInstances[0].ID != tc.wantID {
				t.Fatalf("wrong current instance: %+v", row.CurrentInstances)
			}
			if len(row.Instances) != 2 {
				t.Fatal("lost actual observations used for restore conflict checks")
			}
		})
	}
}
