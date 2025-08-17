import { BrowserRouter as Router, Routes, Route } from 'react-router-dom';
import NavBar from './components/NavBar';
import StatusBar from './components/StatusBar';
import Report from './pages/Report';
import TaskDetail from './pages/TaskDetail';
import './App.css';

function App() {
  return (
    <Router>
      <NavBar />
      <main className="pt-[44px] pb-14 min-h-screen bg-background text-primary">
        <Routes>
          <Route path="/report" element={<Report />} />
          <Route path="/tasks/:id" element={<TaskDetail />} />
          <Route path="*" element={
            <div className="px-0 bg-surface text-primary rounded shadow-md max-w-xl mx-auto mt-10 p-6">
              <h2 className="text-2xl font-bold mb-2 text-center py-4 text-primary">Welcome to TaskSync</h2>
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
