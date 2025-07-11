export default function StatusBar() {
  return (
    <footer className="fixed bottom-0 left-0 w-full bg-surface/90 backdrop-blur z-50 border-t border-border shadow-inner h-10 flex items-center px-6 text-sm text-muted">
      <span>Status: Ready</span>
      {/* You can add live status info, WebSocket state, etc. here */}
    </footer>
  );
}
