export default function StatusBar() {
  return (
    <footer className="fixed bottom-0 left-0 w-full bg-white/80 dark:bg-gray-900/80 backdrop-blur z-50 border-t border-gray-200 dark:border-gray-700 shadow-inner h-10 flex items-center px-6 text-sm text-gray-700 dark:text-gray-300">
      <span>Status: Ready</span>
      {/* You can add live status info, WebSocket state, etc. here */}
    </footer>
  );
}
