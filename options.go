package radius


type ServerOption func(*Server) 



var WithDefaultPacketParser = func (s *Server) {
	s.PacketParser = Parse
}


func WithPacketParser(h ParseFunc)  ServerOption {
	return func(s *Server) {
		s.PacketParser = h
	}
}