import { BrowserRouter as Router, Routes, Route } from 'react-router-dom';
import NavBar from './components/NavBar';
import StatusBar from './components/StatusBar';
import Report from './pages/Report';
import './App.css';

function App() {
  return (
    <Router>
      <NavBar />
      <main className="pt-[44px] min-h-screen bg-gray-50 dark:bg-gray-950">
        <Routes>
          <Route path="/report" element={<Report />} />
          <Route path="*" element={
            <div className="px-0">
              <h2 className="text-2xl font-bold mb-2 text-center py-4">Welcome to TaskSync</h2>
              <p className="text-center">Select "Report" to view all tasks.</p>
            </div>
          } />
        </Routes>
      </main>
      <StatusBar />
    </Router>
  );
}

export default App
