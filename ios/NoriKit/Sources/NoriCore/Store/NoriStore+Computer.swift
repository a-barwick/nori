import Foundation

extension NoriStore {
    @discardableResult
    public func refreshComputerStatus() async -> ComputerStatus? {
        let generation = sessionGeneration
        do {
            let status = try await transport.fetchComputerStatus()
            guard generation == sessionGeneration, !Task.isCancelled else { return nil }
            computerStatus = status
            return status
        } catch {
            guard generation == sessionGeneration, !Task.isCancelled else { return nil }
            _ = await handleAuthFailureIfNeeded(error)
            return computerStatus
        }
    }

    @discardableResult
    public func wakeComputer() async throws -> ComputerStatus {
        let generation = sessionGeneration
        do {
            let status = try await transport.wakeComputer()
            guard generation == sessionGeneration else { throw CancellationError() }
            computerStatus = status
            showToast("Computer is awake")
            return status
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleOperationError(error)
            throw error
        }
    }

    public func sleepComputer() async throws {
        let generation = sessionGeneration
        do {
            try await transport.sleepComputer()
            guard generation == sessionGeneration else { throw CancellationError() }
            if var current = computerStatus {
                current.status = .sleeping
                computerStatus = current
            }
            showToast("Computer is asleep")
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleOperationError(error)
            throw error
        }
    }

    @discardableResult
    public func resetComputer() async throws -> ComputerStatus {
        let generation = sessionGeneration
        do {
            let status = try await transport.resetComputer()
            guard generation == sessionGeneration else { throw CancellationError() }
            computerStatus = status
            showToast("Computer reset")
            return status
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleOperationError(error)
            throw error
        }
    }

    public func listComputerCheckpoints() async throws -> [ComputerCheckpoint] {
        let generation = sessionGeneration
        do {
            let checkpoints = try await transport.listComputerCheckpoints()
            guard generation == sessionGeneration, !Task.isCancelled else { throw CancellationError() }
            return checkpoints
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleOperationError(error)
            throw error
        }
    }

    @discardableResult
    public func createComputerCheckpoint(label: String? = nil) async throws -> ComputerCheckpoint {
        let generation = sessionGeneration
        do {
            let ckpt = try await transport.createComputerCheckpoint(label: label)
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await refreshComputerStatus()
            showToast("Checkpoint saved")
            return ckpt
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleOperationError(error)
            throw error
        }
    }

    @discardableResult
    public func restoreComputerCheckpoint(checkpointID: String? = nil) async throws -> ComputerStatus {
        let generation = sessionGeneration
        do {
            let status = try await transport.restoreComputerCheckpoint(checkpointID: checkpointID)
            guard generation == sessionGeneration else { throw CancellationError() }
            computerStatus = status
            showToast("Checkpoint restored")
            return status
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleOperationError(error)
            throw error
        }
    }

    public func createComputerSession(
        mode: ComputerSessionMode = .view,
        taskID: String? = nil,
        interactionID: String? = nil
    ) async throws -> CreateComputerSessionResponse {
        try await transport.createComputerSession(
            CreateComputerSessionRequest(
                mode: mode,
                taskID: taskID,
                interactionID: interactionID
            )
        )
    }

    public func updateComputerSessionMode(
        sessionID: String,
        mode: ComputerSessionMode
    ) async throws -> ComputerSessionSummary {
        try await transport.updateComputerSessionMode(sessionID: sessionID, mode: mode)
    }

    public func endComputerSession(sessionID: String) async {
        try? await transport.endComputerSession(sessionID: sessionID)
    }

    public func computerDesktopWebSocketRequest(for session: CreateComputerSessionResponse) async throws -> URLRequest {
        try await transport.computerDesktopWebSocketRequest(for: session)
    }
}
