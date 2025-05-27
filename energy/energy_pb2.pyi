from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from typing import ClassVar as _ClassVar, Iterable as _Iterable, Mapping as _Mapping, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class PredictionRequest(_message.Message):
    __slots__ = ("cpu_model", "ram_capacity_gb", "cpu_freq_mhz", "num_cores", "achieved_load_pct")
    CPU_MODEL_FIELD_NUMBER: _ClassVar[int]
    RAM_CAPACITY_GB_FIELD_NUMBER: _ClassVar[int]
    CPU_FREQ_MHZ_FIELD_NUMBER: _ClassVar[int]
    NUM_CORES_FIELD_NUMBER: _ClassVar[int]
    ACHIEVED_LOAD_PCT_FIELD_NUMBER: _ClassVar[int]
    cpu_model: str
    ram_capacity_gb: float
    cpu_freq_mhz: int
    num_cores: int
    achieved_load_pct: float
    def __init__(self, cpu_model: _Optional[str] = ..., ram_capacity_gb: _Optional[float] = ..., cpu_freq_mhz: _Optional[int] = ..., num_cores: _Optional[int] = ..., achieved_load_pct: _Optional[float] = ...) -> None: ...

class PredictionResponse(_message.Message):
    __slots__ = ("predicted_power_watts", "status", "error")
    PREDICTED_POWER_WATTS_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    predicted_power_watts: float
    status: str
    error: str
    def __init__(self, predicted_power_watts: _Optional[float] = ..., status: _Optional[str] = ..., error: _Optional[str] = ...) -> None: ...

class BatchPredictionRequest(_message.Message):
    __slots__ = ("requests",)
    REQUESTS_FIELD_NUMBER: _ClassVar[int]
    requests: _containers.RepeatedCompositeFieldContainer[PredictionRequest]
    def __init__(self, requests: _Optional[_Iterable[_Union[PredictionRequest, _Mapping]]] = ...) -> None: ...

class BatchPredictionResponse(_message.Message):
    __slots__ = ("predictions", "status", "error")
    PREDICTIONS_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    predictions: _containers.RepeatedScalarFieldContainer[float]
    status: str
    error: str
    def __init__(self, predictions: _Optional[_Iterable[float]] = ..., status: _Optional[str] = ..., error: _Optional[str] = ...) -> None: ...
