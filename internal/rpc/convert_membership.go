package rpc

import (
	"github.com/talifpathan/helix/internal/membership"
	helixv1 "github.com/talifpathan/helix/internal/rpc/helixv1"
)

func toProtoState(s membership.State) helixv1.MemberState {
	switch s {
	case membership.Suspect:
		return helixv1.MemberState_MEMBER_STATE_SUSPECT
	case membership.Dead:
		return helixv1.MemberState_MEMBER_STATE_DEAD
	default:
		return helixv1.MemberState_MEMBER_STATE_ALIVE
	}
}

func fromProtoState(s helixv1.MemberState) membership.State {
	switch s {
	case helixv1.MemberState_MEMBER_STATE_SUSPECT:
		return membership.Suspect
	case helixv1.MemberState_MEMBER_STATE_DEAD:
		return membership.Dead
	default:
		return membership.Alive
	}
}

func toProtoUpdates(us []membership.Update) []*helixv1.Update {
	out := make([]*helixv1.Update, 0, len(us))
	for _, u := range us {
		out = append(out, &helixv1.Update{
			Id:          u.ID,
			Incarnation: u.Incarnation,
			State:       toProtoState(u.State),
		})
	}
	return out
}

func fromProtoUpdates(pus []*helixv1.Update) []membership.Update {
	out := make([]membership.Update, 0, len(pus))
	for _, pu := range pus {
		out = append(out, membership.Update{
			ID:          pu.GetId(),
			Incarnation: pu.GetIncarnation(),
			State:       fromProtoState(pu.GetState()),
		})
	}
	return out
}
