package srv

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"premai.io/Ayup/go/internal/assist"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
)

func (s *Srv) AssistantsList(ctx context.Context, req *pb.AssistantsListReq) (*pb.AssistantsListResp, error) {
	assistants := s.registry.List()

	infos := make([]*pb.AssistantInfo, len(assistants))
	for i, v := range assistants {
		infos[i] = &pb.AssistantInfo{
			Name: v.Name(),
		}
	}

	return &pb.AssistantsListResp{
		Assistants: infos,
	}, nil
}

func (s *Srv) AssistantsPush(ctx context.Context, req *pb.AssistantsPushReq) (*pb.AssistantsPushResp, error) {
	nameBs, err := assist.LoadName(ctx, s.AssistantDir)
	if err != nil {
		return nil, err
	}
	shaName := fmt.Sprintf("%x", sha256.Sum256(nameBs))
	path := filepath.Join(s.LocalAssistantsDir, shaName)

	if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
		return nil, terror.Errorf(ctx, "os RemoveAll: %w", err)
	}

	if err := os.Rename(s.AssistantDir, path); err != nil {
		return nil, terror.Errorf(ctx, "os Rename: %w", err)
	}

	s.registry.Del(assist.FullName(assist.Local, string(nameBs)))
	if _, err := s.registry.RegisterDir(ctx, assist.Local, path); err != nil {
		return nil, err
	}

	return &pb.AssistantsPushResp{}, nil
}
